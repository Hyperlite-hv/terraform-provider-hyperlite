# By the file's name; the same led by `<node>/` for one on another node of the cluster. `source` is not read back:
# give the configuration the file it was uploaded from.
terraform import hyperlite_iso.debian debian-13.1.0-amd64-netinst.iso
terraform import hyperlite_iso.debian pve-b/debian-13.1.0-amd64-netinst.iso
