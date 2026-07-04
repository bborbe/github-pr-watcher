ARG DOCKER_REGISTRY=docker.io
FROM ${DOCKER_REGISTRY}/golang:1.26.4 AS build
ARG BUILD_GIT_COMMIT=none
ARG BUILD_DATE=unknown
COPY . /workspace
WORKDIR /workspace
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -mod=vendor -ldflags "-s" -a -installsuffix cgo -o /main
CMD ["/bin/bash"]

FROM ${DOCKER_REGISTRY}/alpine:3.23 AS alpine
RUN apk --no-cache add ca-certificates curl bash \
 && rm -rf /tmp/*

FROM alpine
ARG BUILD_GIT_COMMIT=none
ARG BUILD_DATE=unknown
COPY --from=build /main /main
ENV ZONEINFO=/zoneinfo.zip
COPY --from=build /usr/local/go/lib/time/zoneinfo.zip /
ENV BUILD_GIT_COMMIT=${BUILD_GIT_COMMIT}
ENV BUILD_DATE=${BUILD_DATE}
ENTRYPOINT ["/main"]
