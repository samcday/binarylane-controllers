FROM rust:1-alpine AS build
RUN apk add --no-cache build-base cmake musl-dev protobuf-dev

WORKDIR /src

# Copy manifests, build script, and proto definitions first for layer caching
COPY Cargo.toml Cargo.lock build.rs ./
COPY xtask/Cargo.toml xtask/Cargo.toml
COPY proto/ proto/

# Create dummy sources so cargo can fetch + compile all dependencies
RUN mkdir src && echo "fn main() {}" > src/main.rs && \
    mkdir xtask/src && echo "fn main() {}" > xtask/src/main.rs && \
    cargo build --release -p binarylane-controller && \
    rm -rf src xtask/src target/release/.fingerprint/binarylane-controller-*

# Now copy real sources — only our crate recompiles
# Recreate xtask dummy (not built, but workspace needs valid member)
RUN mkdir xtask/src && echo "fn main() {}" > xtask/src/main.rs
COPY src/ src/
RUN cargo build --release -p binarylane-controller

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /src/target/release/binarylane-controller /binarylane-controller
ENTRYPOINT ["/binarylane-controller"]
