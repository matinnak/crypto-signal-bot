# Always-on deployment (serve mode): instant replies and checks every minute.
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /signalbot ./cmd/signalbot

FROM gcr.io/distroless/static
COPY --from=build /signalbot /signalbot
VOLUME /data
ENTRYPOINT ["/signalbot", "-mode", "serve", "-state", "/data/state.json"]
