group "default" {
  targets = ["agent", "controller", "coredns"]
}

target "base" {
  context = "."
}

target "agent" {
  inherits   = ["base"]
  dockerfile = "images/Dockerfile.agent"
  tags       = ["clusterip-gw-agent:latest"]
}

target "controller" {
  inherits   = ["base"]
  dockerfile = "images/Dockerfile.controller"
  tags       = ["clusterip-gw-controller:latest"]
}

target "coredns" {
  inherits   = ["base"]
  dockerfile = "images/Dockerfile.coredns"
  tags       = ["clusterip-gw-coredns:latest"]
}
