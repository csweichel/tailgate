# tailgate

`tailgate` gives one local HTTP service a private Tailscale HTTPS endpoint. It
runs its own userspace Tailscale node with `tsnet`, obtains the node certificate
through Tailscale, and reverse-proxies HTTPS requests to a single downstream
service on `127.0.0.1:8080` by default.

It can also start and supervise the downstream command, making it suitable as a
container entrypoint or a small systemd service. Arguments after `--` are the
command to run. Signals are forwarded to its process group and the binary exits
with the downstream command's exit code.

## Usage

Enable MagicDNS and HTTPS for the tailnet, then create a reusable or ephemeral
auth key. The key needs any tags required by your tailnet ACLs.

```sh
export TS_AUTHKEY=tskey-auth-...
tailgate --hostname my-service -- ./my-service --port 8080
```

To proxy an already-running service:

```sh
TS_AUTHKEY=tskey-auth-... tailgate --hostname my-service --downstream-port 3000
```

The service is then available at `https://my-service.<tailnet-name>.ts.net/` to
peers allowed by your Tailscale policy. Nothing is exposed on the host network;
the HTTPS listener exists only inside the tailnet.

Options:

```text
--auth-key string         Tailscale auth key (default: TS_AUTHKEY)
--hostname string         tailnet hostname (default: TS_HOSTNAME or OS hostname)
--state-dir string        persistent Tailscale state (default: TS_STATE_DIR or /var/lib/tailgate)
--listen string           tailnet HTTPS address (default: :443)
--downstream-host string  downstream host (default: 127.0.0.1)
--downstream-port int     downstream HTTP port (default: 8080)
--ephemeral               register an ephemeral Tailscale node
```

Persist `/var/lib/tailgate` for a stable node identity. If `--ephemeral` is
used, use an ephemeral auth key as well.

## Container entrypoint

```dockerfile
COPY --from=tailgate-build /out/tailgate /usr/local/bin/tailgate
ENTRYPOINT ["/usr/local/bin/tailgate", "--hostname", "my-service", "--"]
CMD ["/usr/local/bin/my-service", "--port", "8080"]
```

Pass `TS_AUTHKEY` at runtime and mount a volume at `/var/lib/tailgate`. The
container needs outbound network access but no TUN device or Tailscale daemon.

## systemd

```ini
[Unit]
Description=Private HTTPS endpoint for my-service
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=/etc/tailgate/my-service.env
ExecStart=/usr/local/bin/tailgate --hostname my-service -- /usr/local/bin/my-service --port 8080
Restart=on-failure
StateDirectory=tailgate

[Install]
WantedBy=multi-user.target
```

Set `TS_AUTHKEY=...` in the root-readable environment file. Releases are built
for Linux amd64 and arm64 when a `v*` tag is pushed.
