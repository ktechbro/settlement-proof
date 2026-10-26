FROM golang:1.27.3-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/indexer ./cmd/indexer && CGO_ENABLED=0 go build -trimpath -o /out/demo ./cmd/demo

FROM alpine:3.23
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 observer
WORKDIR /app
COPY --from=build /out/ /app/
USER observer
EXPOSE 8080
ENTRYPOINT ["/app/indexer"]
