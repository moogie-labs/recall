#!/bin/sh
# Developer regression gate: populated normal Go caches, no exported cache paths.
set -eu
cd "$(dirname "$0")/.."
profile='(version 1)(allow default)(deny network-outbound (remote ip "*:*"))'
/usr/bin/sandbox-exec -p "$profile" python3 -c 'import errno,socket; code=socket.socket().connect_ex(("1.1.1.1",443)); print("Outbound TCP denial errno:",code); assert code in (errno.EPERM,errno.EACCES)'
exec env -u GOPATH -u GOMODCACHE /usr/bin/sandbox-exec -p "$profile" go test ./integration "$@"
