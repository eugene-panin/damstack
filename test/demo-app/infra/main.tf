terraform {
  backend "local" {}
}

variable "project" {
  type = string
}

variable "hostname" {
  type = string
}

variable "greeting" {
  type    = string
  default = "no greeting from the platform"
}

variable "token" {
  type      = string
  sensitive = true
}

resource "terraform_data" "app" {
  input = "${var.greeting} from ${var.hostname}"
}

output "message" {
  value = terraform_data.app.output
}

output "token_length" {
  value = nonsensitive(length(var.token))
}

output "dns_records" {
  value = {
    (var.hostname) = [{ type = "A", name = var.hostname, content = "10.0.0.9" }]
  }
}
