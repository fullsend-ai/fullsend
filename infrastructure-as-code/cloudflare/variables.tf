variable "cloudflare" {
  type = object({
    zone = optional(string, "fullsend.sh")
  })
  default     = {}
  nullable    = false
  description = "Cloudflare configuration"
}
