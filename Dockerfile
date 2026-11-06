FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /ps5-tv-sleep ./cmd/ps5-tv-sleep

FROM alpine:3.22
RUN apk add --no-cache android-tools \
    && adduser -D -h /data app \
    && mkdir /data/.android \
    && chown app:app /data/.android
COPY --from=build /ps5-tv-sleep /usr/local/bin/ps5-tv-sleep
USER app
# The adb key lives here; losing it means approving the prompt on the TV again.
VOLUME /data/.android
ENTRYPOINT ["ps5-tv-sleep"]
