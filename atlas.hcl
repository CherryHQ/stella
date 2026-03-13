variable "dev_url" {
  type    = string
  default = "sqlite://dev?mode=memory"
}

env "local" {
  src = "file://db/schemas/main.sql"
  dev = var.dev_url

  migration {
    dir = "file://db/migrations"
  }
}
