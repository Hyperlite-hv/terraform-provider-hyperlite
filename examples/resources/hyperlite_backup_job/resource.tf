# Every VM tagged `prod`, but one, every night to a NAS mount.
resource "hyperlite_backup_job" "prod" {
  name            = "prod-nightly"
  selection       = "tag"
  value           = "prod"
  excluded        = ["scratch-1"]
  frequency       = "daily"
  time            = "01:00"
  target_dir      = "/mnt/nas/backups"
  retention_count = 7
}
