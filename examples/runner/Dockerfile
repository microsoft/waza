# The runtime base is the OCI empty filesystem, not a mutable distribution tag.
# Build context contains only the statically linked binary built by build.sh.
FROM scratch
COPY --chmod=0555 waza /waza
ENV TMPDIR=/work
USER 65532:65532
WORKDIR /work
ENTRYPOINT ["/waza", "runner", "run"]
