// Force bun's --compile bundler to include steam-user's transitive deps.
// steam-user uses dynamic/conditional requires that the bundler misses.
// Importing for side effects is enough to drag each module into the bundle.
import "steamid";
import "@bbob/parser";
import "@doctormckay/stdlib";
import "@doctormckay/stdlib/http";
import "@doctormckay/steam-crypto";
import "adm-zip";
import "binarykvparser";
import "bytebuffer";
import "file-manager";
import "kvparser";
import "socks-proxy-agent";
import "steam-appticket";
import "steam-session";
import "steam-totp";
import "websocket13";
import "zstddec";
