variable "dev_url" {
  type    = string
  default = "sqlite://dev?mode=memory"
}

env "local" {
  src = "file://internal/db/schemas/main.sql"
  dev = var.dev_url

  migration {
    dir = "file://internal/db/migrations"
  }
}
