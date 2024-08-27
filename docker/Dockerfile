FROM alpine:latest
RUN apk update && apk add ca-certificates
ARG TARGETARCH
COPY ./anklet_linux_${TARGETARCH} /usr/local/bin/anklet
ENTRYPOINT ["/usr/local/bin/anklet"]