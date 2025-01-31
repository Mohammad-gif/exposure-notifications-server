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

// Package observability sets up and configures observability tools.
package observability

import "context"

// Compile-time check to verify implements interface.
var _ Exporter = (*noopExporter)(nil)

// noopExporter is an observability exporter that does nothing.
type noopExporter struct{}

func NewNoop(_ context.Context) (Exporter, error) {
	return &noopExporter{}, nil
}

func (g *noopExporter) StartExporter(_ context.Context) error {
	return nil
}

func (g *noopExporter) Close() error {
	return nil
}
