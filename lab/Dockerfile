FROM alpine:3.22
RUN apk add --no-cache android-tools socat \
    && printf '#!/bin/sh\nexec sh /lab/tv.sh "$@"\n' >/usr/local/bin/tv \
    && chmod +x /usr/local/bin/tv
