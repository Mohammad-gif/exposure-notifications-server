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

// Package database is a database interface to federation in.
package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/exposure-notifications-server/internal/federationin/model"
	"github.com/google/exposure-notifications-server/internal/pb/federation"
	"github.com/google/exposure-notifications-server/pkg/database"
	pgx "github.com/jackc/pgx/v4"
)

type FederationInDB struct {
	db *database.DB
}

func New(db *database.DB) *FederationInDB {
	return &FederationInDB{
		db: db,
	}
}

// FinalizeSyncFn is used to finalize a historical sync record.
type FinalizeSyncFn func(state *federation.FetchState, q *model.FederationInQuery, totalInserted int) error

type queryRowFn func(ctx context.Context, query string, args ...interface{}) pgx.Row

// Lock acquires lock with given name that times out after ttl. Returns an UnlockFn that can be used to unlock the lock. ErrAlreadyLocked will be returned if there is already a lock in use.
func (db *FederationInDB) Lock(ctx context.Context, lockID string, ttl time.Duration) (database.UnlockFn, error) {
	return db.db.Lock(ctx, lockID, ttl)
}

// GetFederationInQuery returns a query for given queryID. If not found, ErrNotFound will be returned.
func (db *FederationInDB) GetFederationInQuery(ctx context.Context, queryID string) (*model.FederationInQuery, error) {
	var query *model.FederationInQuery

	if err := db.db.InTx(ctx, pgx.ReadCommitted, func(tx pgx.Tx) error {
		var err error
		query, err = getFederationInQuery(ctx, queryID, tx.QueryRow)
		return err
	}); err != nil {
		return nil, fmt.Errorf("get federation in query: %w", err)
	}

	return query, nil
}

func getFederationInQuery(ctx context.Context, queryID string, queryRow queryRowFn) (*model.FederationInQuery, error) {
	row := queryRow(ctx, `
		SELECT
			query_id, server_addr, oidc_audience, include_regions, exclude_regions,
			only_local_provenance, only_travelers,
			last_timestamp, primary_cursor, last_revised_timestamp, revised_cursor
		FROM
			FederationInQuery
		WHERE
			query_id=$1
		`, queryID)

	var lastTimestamp, revisedTimestamp *time.Time
	var lastCursor, revisedCursor *string

	// See https://www.opsdash.com/blog/postgres-arrays-golang.html for working with Postgres arrays in Go.
	q := model.FederationInQuery{}
	if err := row.Scan(&q.QueryID, &q.ServerAddr, &q.Audience, &q.IncludeRegions, &q.ExcludeRegions,
		&q.OnlyLocalProvenance, &q.OnlyTravelers,
		&lastTimestamp, &lastCursor, &revisedTimestamp, &revisedCursor); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, database.ErrNotFound
		}
		return nil, fmt.Errorf("scanning results: %w", err)
	}
	if lastTimestamp != nil {
		q.LastTimestamp = *lastTimestamp
	}
	if lastCursor != nil {
		q.LastCursor = *lastCursor
	}
	if revisedTimestamp != nil {
		q.LastRevisedTimestamp = *revisedTimestamp
	}
	if revisedCursor != nil {
		q.LastRevisedCursor = *revisedCursor
	}

	return &q, nil
}

// AddFederationInQuery adds a FederationInQuery entity. It will overwrite a query with matching q.queryID if it exists.
func (db *FederationInDB) AddFederationInQuery(ctx context.Context, q *model.FederationInQuery) error {
	return db.db.InTx(ctx, pgx.ReadCommitted, func(tx pgx.Tx) error {
		query := `
			INSERT INTO
				FederationInQuery
				(query_id, server_addr, oidc_audience, include_regions, exclude_regions, only_local_provenance, only_travelers)
			VALUES
				($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT
				(query_id)
			DO UPDATE
				SET server_addr = $2, oidc_audience = $3, include_regions = $4, exclude_regions = $5, only_local_provenance = $6, only_travelers = $7
		`
		_, err := tx.Exec(ctx, query, q.QueryID, q.ServerAddr, q.Audience, q.IncludeRegions, q.ExcludeRegions, q.OnlyLocalProvenance, q.OnlyTravelers)
		if err != nil {
			return fmt.Errorf("upserting federation query: %w", err)
		}
		return nil
	})
}

// GetFederationInSync returns a federation sync record for given syncID. If not found, ErrNotFound will be returned.
func (db *FederationInDB) GetFederationInSync(ctx context.Context, syncID int64) (*model.FederationInSync, error) {
	var sync *model.FederationInSync

	if err := db.db.InTx(ctx, pgx.ReadCommitted, func(tx pgx.Tx) error {
		var err error
		sync, err = getFederationInSync(ctx, syncID, tx.QueryRow)
		return err
	}); err != nil {
		return nil, fmt.Errorf("get federation in sync: %w", err)
	}

	return sync, nil
}

func getFederationInSync(ctx context.Context, syncID int64, queryRowContext queryRowFn) (*model.FederationInSync, error) {
	row := queryRowContext(ctx, `
		SELECT
			sync_id, query_id, started, completed, insertions, max_timestamp, max_revised_timestamp
		FROM
			FederationInSync
		WHERE
			sync_id=$1
		`, syncID)

	s := model.FederationInSync{}
	var (
		completed, max, maxRevised *time.Time
		insertions                 *int
	)
	if err := row.Scan(&s.SyncID, &s.QueryID, &s.Started, &completed, &insertions, &max, &maxRevised); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, database.ErrNotFound
		}
		return nil, fmt.Errorf("scanning results: %w", err)
	}
	if completed != nil {
		s.Completed = *completed
	}
	if max != nil {
		s.MaxTimestamp = *max
	}
	if maxRevised != nil {
		s.MaxRevisedTimestamp = *maxRevised
	}
	if insertions != nil {
		s.Insertions = *insertions
	}
	return &s, nil
}

// unixToTimestamp converts a unix timestamp into a time.Time
func unixToTimestamp(unixTS int64) *time.Time {
	ts := time.Unix(unixTS, 0).UTC().Truncate(time.Second)
	return &ts
}

// StartFederationInSync stores a historical record of a query sync starting. It returns a FederationInSync key, and a FinalizeSyncFn that must be invoked to finalize the historical record.
func (db *FederationInDB) StartFederationInSync(ctx context.Context, q *model.FederationInQuery, started time.Time) (int64, FinalizeSyncFn, error) {
	conn, err := db.db.Pool.Acquire(ctx)
	if err != nil {
		return 0, nil, fmt.Errorf("acquiring connection: %w", err)
	}
	defer conn.Release()

	// startedTime is used internally to compute a Duration between here and when finalize function below is executed.
	// This allows the finalize function to not request a completed Time parameter.
	startedTimer := time.Now()

	var syncID int64
	row := conn.QueryRow(ctx, `
		INSERT INTO
			FederationInSync
			(query_id, started)
		VALUES
			($1, $2)
		RETURNING sync_id
		`, q.QueryID, started)
	if err := row.Scan(&syncID); err != nil {
		return 0, nil, fmt.Errorf("fetching sync_id: %w", err)
	}

	finalize := func(state *federation.FetchState, q *model.FederationInQuery, totalInserted int) error {
		completed := started.Add(time.Since(startedTimer))

		return db.db.InTx(ctx, pgx.ReadCommitted, func(tx pgx.Tx) error {
			// Special case: when no keys are pulled, the maxTimestamp will be 0, so we don't update the
			// FederationQuery in this case to prevent it from going back and fetching old keys from the past.
			if totalInserted > 0 {
				q.UpdateFetchState(state)
				_, err := tx.Exec(ctx, `
					UPDATE
						FederationInQuery
					SET
						last_timestamp = $1, primary_cursor = $2,
						last_revised_timestamp = $3, revised_cursor = $4
					WHERE
						query_id = $5
					`, q.LastTimestamp, q.LastCursor, q.LastRevisedTimestamp, q.LastRevisedCursor, q.QueryID)
				if err != nil {
					return fmt.Errorf("updating federation query state: %w", err)
				}
			}

			var max, maxRevised *time.Time
			if totalInserted > 0 {
				max = unixToTimestamp(state.KeyCursor.Timestamp)
				maxRevised = unixToTimestamp(state.RevisedKeyCursor.Timestamp)
			}
			_, err = tx.Exec(ctx, `
				UPDATE
					FederationInSync
				SET
					completed = $1,
					insertions = $2,
					max_timestamp = $3,
					max_revised_timestamp = $4
				WHERE
					sync_id = $5
			`, completed, totalInserted, max, maxRevised, syncID)
			if err != nil {
				return fmt.Errorf("updating federation sync: %w", err)
			}
			return nil
		})
	}

	return syncID, finalize, nil
}
