# By the template's name; the same led by `<node>/` for one on another node of the cluster.
terraform import hyperlite_template.golden debian-golden
terraform import hyperlite_template.golden pve-b/debian-golden
