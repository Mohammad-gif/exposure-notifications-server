# Copyright 2020 the Exposure Notifications Server authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# protoc is a base container with protoc and protoc-gen-go installed.

FROM golang:1.16 AS builder

ENV PROTOC_VERSION "3.11.4"
ENV PROTOC_GEN_GO_VERSION "1.4.1"

RUN apt-get update -yqq && \
  apt-get install -yqq curl git unzip

# Install protoc
RUN curl -sfLo protoc.zip "https://github.com/protocolbuffers/protobuf/releases/download/v${PROTOC_VERSION}/protoc-${PROTOC_VERSION}-linux-x86_64.zip" && \
  mkdir protoc && \
  unzip -q -d protoc protoc.zip

# Install gen-go
RUN git clone -q https://github.com/golang/protobuf && \
  cd protobuf && \
  git checkout -q tags/v${PROTOC_GEN_GO_VERSION} -b build && \
  go build -o /go/bin/protoc-gen-go ./protoc-gen-go



FROM debian:buster-slim

COPY --from=builder /go/protoc/include/google /usr/local/include/google
COPY --from=builder /go/protoc/bin/protoc /usr/local/bin/protoc
COPY --from=builder /go/bin/protoc-gen-go /usr/local/bin/protoc-gen-go

ENTRYPOINT ["/usr/local/bin/protoc"]
