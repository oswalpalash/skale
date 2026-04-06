FROM golang:1.26.0 AS build

ARG VERSION=development
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
  -ldflags="-s -w -X github.com/oswalpalash/skale/internal/version.Version=${VERSION} -X github.com/oswalpalash/skale/internal/version.Commit=${COMMIT} -X github.com/oswalpalash/skale/internal/version.BuildDate=${BUILD_DATE}" \
  -o /out/controller ./cmd/controller

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/controller /controller

ENTRYPOINT ["/controller"]
