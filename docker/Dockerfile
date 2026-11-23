FROM alpine:3.21
RUN apk add --no-cache ca-certificates && \
    addgroup -S anklet && adduser -S -G anklet anklet
ARG TARGETARCH
COPY --chmod=0755 ./anklet_linux_${TARGETARCH} /usr/local/bin/anklet
USER anklet
ENTRYPOINT ["/usr/local/bin/anklet"]
