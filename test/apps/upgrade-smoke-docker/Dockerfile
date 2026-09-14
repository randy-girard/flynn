# Container-stack app for vagrant upgrade smoke.
# RUN apk exercises BuildKit's nested runc on slimmed dockerbuilder-24.
FROM alpine:3.20
RUN apk add --no-cache busybox-extras wget
COPY start.sh /start.sh
EXPOSE 8080
CMD ["/bin/sh", "/start.sh"]
