# Scratch is the OCI empty filesystem, not a mutable distribution tag.
# The only build input is the static binary produced by build.sh.
FROM scratch
ARG SOURCE_DATE_EPOCH
ARG REVISION
LABEL org.opencontainers.image.source="https://github.com/microsoft/waza" \
      org.opencontainers.image.revision=$REVISION \
      org.opencontainers.image.description="Bounded public runner contract reference (mock executor only)"
COPY --chmod=0555 waza /waza
ENV TMPDIR=/work
USER 65532:65532
WORKDIR /work
ENTRYPOINT ["/waza", "runner", "run"]
