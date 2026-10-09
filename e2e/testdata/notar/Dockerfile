# sh and coreutils, but no tar: exercises the inject strategy.
FROM debian:bookworm-slim
RUN rm -f /usr/bin/tar /bin/tar && ! command -v tar
