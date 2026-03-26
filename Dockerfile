FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go mod tidy && CGO_ENABLED=0 go build -o /binarylane-controller .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /binarylane-controller /binarylane-controller
ENTRYPOINT ["/binarylane-controller"]
