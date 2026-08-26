FROM --platform=$BUILDPLATFORM golang:1.26.1-bookworm AS build
ARG TARGETOS
ARG TARGETARCH
ARG GOPROXY=https://goproxy.cn,direct
WORKDIR /src
COPY go.mod go.sum ./
RUN GOPROXY=$GOPROXY go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/sports-hub ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/sports-hub /app/sports-hub
USER nonroot:nonroot
EXPOSE 8080
ENV HTTP_ADDR=:8080 DB_PATH=/tmp/sports-hub.db
ENTRYPOINT ["/app/sports-hub"]
