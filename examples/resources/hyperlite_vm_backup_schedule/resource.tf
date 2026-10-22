resource "hyperlite_vm_backup_schedule" "web" {
  vm              = hyperlite_vm.web.name
  frequency       = "daily"
  time            = "02:30"
  retention_count = 7
  keep_weeks      = 4
  keep_months     = 6
}
