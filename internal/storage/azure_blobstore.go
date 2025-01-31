// Copyright 2020 the Exposure Notifications Server authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// +build azure all

package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"time"

	"github.com/Azure/azure-storage-blob-go/azblob"
	"github.com/Azure/go-autorest/autorest/adal"
	"github.com/google/exposure-notifications-server/pkg/signal"
	"go.opencensus.io/stats"
	"go.uber.org/zap"
)

func init() {
	RegisterBlobstore("AZURE_BLOB_STORAGE", NewAzureBlobstore)
}

// Compile-time check to verify implements interface.
var _ Blobstore = (*AzureBlobstore)(nil)

// AzureBlobstore implements the Blob interface and provides the ability
// write files to Azure Blob Storage.
type AzureBlobstore struct {
	serviceURL *azblob.ServiceURL
}

func newAccessTokenCredential(accountName string, accountKey string) (azblob.Credential, error) {
	credential, err := azblob.NewSharedKeyCredential(accountName, accountKey)
	if err != nil {
		return nil, fmt.Errorf("storage.newAccessTokenCredential: %w", err)
	}
	return credential, nil
}

func newMSITokenCredential(ctx context.Context, blobstoreURL string) (azblob.Credential, error) {
	msiEndpoint, err := adal.GetMSIVMEndpoint()
	if err != nil {
		return nil, fmt.Errorf("failed to get MSI endpoint: %w", err)
	}

	spt, err := adal.NewServicePrincipalTokenFromMSI(msiEndpoint, blobstoreURL)
	if err != nil {
		return nil, fmt.Errorf("failed to get service principal token from msi %v: %w", msiEndpoint, err)
	}

	logger, err := zap.NewProduction()
	if err != nil {
		return nil, fmt.Errorf("failed to configure logger: %w", err)
	}

	tokenRefresher := func(credential azblob.TokenCredential) time.Duration {
		err := spt.Refresh()
		if err != nil {
			stats.Record(ctx, mAzureRefreshFailed.M(1))
			logger.Error("failed to refresh access token",
				zap.String("error", err.Error()))

			token := spt.Token()
			if token.Expires().After(time.Now().UTC()) {
				stats.Record(ctx, mAzureRefreshExpired.M(1))

				logger.Error("access token expired - shutting down server")
				if err := signal.SendInterrupt(); err != nil {
					// extreme measures.
					logger.Fatal("failed to shut down server gracefully, killing",
						zap.String("error", err.Error()))
				}
			}

			// Retry again in 15 seconds.
			// Max of ~8 retries since refresh is normally scheduled for 2 minutes
			// prior to expiration.
			return 15 * time.Second
		}

		token := spt.Token()
		credential.SetToken(token.AccessToken)

		exp := token.Expires().UTC().Sub(time.Now().UTC().Add(2 * time.Minute))
		return exp
	}

	return azblob.NewTokenCredential("", tokenRefresher), nil
}

// NewAzureBlobstore creates a storage client, suitable for use with
// serverenv.ServerEnv.
func NewAzureBlobstore(ctx context.Context, _ *Config) (Blobstore, error) {
	accountName := os.Getenv("AZURE_STORAGE_ACCOUNT")
	if accountName == "" {
		return nil, fmt.Errorf("missing AZURE_STORAGE_ACCOUNT")
	}

	primaryURLRaw := fmt.Sprintf("https://%s.blob.core.windows.net", accountName)
	primaryURL, err := url.Parse(primaryURLRaw)
	if err != nil {
		return nil, fmt.Errorf("failed to parse URL %v: %w", primaryURLRaw, err)
	}

	accountKey := os.Getenv("AZURE_STORAGE_ACCESS_KEY")

	// use the storage account key if provided, otherwise use managed identity
	var credential azblob.Credential
	if accountKey != "" {
		credential, err = newAccessTokenCredential(accountName, accountKey)
		if err != nil {
			return nil, err
		}
	} else {
		credential, err = newMSITokenCredential(ctx, primaryURLRaw)
		if err != nil {
			return nil, err
		}
	}

	p := azblob.NewPipeline(credential, azblob.PipelineOptions{})
	serviceURL := azblob.NewServiceURL(*primaryURL, p)

	return &AzureBlobstore{
		serviceURL: &serviceURL,
	}, nil
}

// CreateObject creates a new blobstore object or overwrites an existing one.
func (s *AzureBlobstore) CreateObject(ctx context.Context, container, name string, contents []byte, cacheable bool, contentType string) error {
	cacheControl := "public, max-age=86400"
	if !cacheable {
		cacheControl = "no-cache, max-age=0"
	}

	blobURL := s.serviceURL.NewContainerURL(container).NewBlockBlobURL(name)
	headers := azblob.BlobHTTPHeaders{
		CacheControl: cacheControl,
	}
	if contentType != "" {
		headers.ContentType = contentType
	}
	if _, err := azblob.UploadBufferToBlockBlob(ctx, contents, blobURL, azblob.UploadToBlockBlobOptions{
		BlobHTTPHeaders: headers,
	}); err != nil {
		return fmt.Errorf("storage.CreateObject: %w", err)
	}
	return nil
}

// DeleteObject deletes a blobstore object, returns nil if the object was
// successfully deleted, or if the object doesn't exist.
func (s *AzureBlobstore) DeleteObject(ctx context.Context, container, name string) error {
	blobURL := s.serviceURL.NewContainerURL(container).NewBlockBlobURL(name)
	if _, err := blobURL.Delete(ctx, azblob.DeleteSnapshotsOptionInclude, azblob.BlobAccessConditions{}); err != nil {
		var terr azblob.StorageError
		if errors.As(err, &terr) && terr.ServiceCode() == azblob.ServiceCodeBlobNotFound {
			// already deleted
			return nil
		}
		return fmt.Errorf("storage.DeleteObject: %w", err)
	}
	return nil
}

// GetObject returns the contents for the given object. If the object does not
// exist, it returns ErrNotFound.
func (s *AzureBlobstore) GetObject(ctx context.Context, container, name string) ([]byte, error) {
	blobURL := s.serviceURL.NewContainerURL(container).NewBlockBlobURL(name)
	dr, err := blobURL.Download(ctx, 0, azblob.CountToEnd, azblob.BlobAccessConditions{}, false, azblob.ClientProvidedKeyOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to download object: %w", err)
	}

	body := dr.Body(azblob.RetryReaderOptions{MaxRetryRequests: 5})
	defer body.Close()

	var b bytes.Buffer
	if _, err := io.Copy(&b, body); err != nil {
		return nil, fmt.Errorf("failed to read object: %w", err)
	}

	return b.Bytes(), nil
}
