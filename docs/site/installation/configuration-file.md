# The configuration file

Every setting in the [configuration reference](configuration.md) can also live
in a YAML file ([ADR 0015](https://github.com/talk2sohail/privoclub/blob/main/docs/adr/0015-configuration.md)).
A commented template with every setting is
[`docs/privo.example.yaml`](https://github.com/talk2sohail/privoclub/blob/main/docs/privo.example.yaml).

```yaml
listen: ":8080"
data_dir: /data
public_url: https://app.example.com
master_key_file: /run/secrets/privo_master_key
backup_interval: 24h
```

```sh
privo --config /etc/privo/privo.yaml serve         # or PRIVO_CONFIG_FILE=/etc/privo/privo.yaml
privo --config /etc/privo/privo.yaml config show   # every setting, its value and its source
```

## The rules

- A key is the variable name in lower case without `PRIVO_`.
- The environment beats the file, which beats the default.
- Nothing is read unless the file is named.
- A named file that is missing, malformed, has an unknown or repeated key, or
  holds a secret stops the command with exit 2. The secrets are
  `master_key`, `master_key_previous` and `bootstrap_token`: name a file
  with the `_file` form instead (`master_key_file`).
- The file is read once at start; restart to change a setting.
- In a container use `PRIVO_CONFIG_FILE`, not `--config`, so the image's
  health check reads the same file.

[Deploy on Coolify](coolify.md#settings-environment-or-a-configuration-file)
shows the file as a Coolify file mount.
