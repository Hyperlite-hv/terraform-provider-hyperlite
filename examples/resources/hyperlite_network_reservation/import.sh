# By `<network>/<mac>`; the same led by `<node>/` for one on another node of the cluster.
terraform import hyperlite_network_reservation.web lan/52:54:00:12:34:56
terraform import hyperlite_network_reservation.web pve-b/lan/52:54:00:12:34:56
