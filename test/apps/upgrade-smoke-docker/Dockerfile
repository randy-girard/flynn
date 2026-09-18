# Container-stack app for vagrant upgrade smoke.
# RUN apk exercises BuildKit's nested runc on slimmed dockerbuilder-24.
# SMOKE_BODY distinguishes git-push (default) from flynn docker push.
# apk talks to dl-cdn.alpinelinux.org; retry so a CDN blip does not fail smoke.
FROM alpine:3.20
ARG SMOKE_BODY="docker-smoke ok"
ENV SMOKE_BODY="${SMOKE_BODY}"
RUN set -eux; \
    i=1; \
    while [ "${i}" -le 5 ]; do \
      apk add --no-cache busybox-extras wget && exit 0; \
      echo "apk add failed (attempt ${i}/5); retrying"; \
      i=$((i + 1)); \
      sleep $((i * 3)); \
    done; \
    exit 1
COPY start.sh /start.sh
EXPOSE 8080
CMD ["/bin/sh", "/start.sh"]
