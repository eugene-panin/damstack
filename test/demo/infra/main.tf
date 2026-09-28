terraform {
  backend "local" {}
}

variable "project" {
  type = string
}

variable "token" {
  type      = string
  sensitive = true
}

locals {
  stack = yamldecode(file("${var.project}/stack.yaml"))
}

resource "terraform_data" "greeting" {
  input = local.stack.greeting
}

output "greeting" {
  value = terraform_data.greeting.output
}

output "token_length" {
  value = nonsensitive(length(var.token))
}
