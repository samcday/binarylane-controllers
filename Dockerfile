# syntax=docker/dockerfile:1
FROM rust:1-alpine AS build
RUN apk add --no-cache build-base musl-dev protobuf-dev

WORKDIR /src

COPY Cargo.toml Cargo.lock build.rs ./
COPY proto/ proto/
COPY binarylane-client/ binarylane-client/

RUN sed -i 's/, "integration-tests"//; s/, "xtask"//' Cargo.toml

COPY src/ src/

RUN --mount=type=cache,target=/src/target \
    --mount=type=cache,target=/usr/local/cargo/registry \
    cargo build --release && \
    cp target/release/binarylane-controller /binarylane-controller

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /binarylane-controller /binarylane-controller
ENTRYPOINT ["/binarylane-controller"]
