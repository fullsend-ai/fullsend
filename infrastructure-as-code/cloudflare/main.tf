provider "cloudflare" {
  # Authenticate using CLOUDFLARE_API_TOKEN environment variable
}

data "cloudflare_zone" "this" {
  filter = {
    name = var.cloudflare.zone
  }
}

resource "cloudflare_zone_setting" "always_use_https" {
  zone_id    = data.cloudflare_zone.this.id
  setting_id = "always_use_https"
  value      = "on"
}
