terraform {
  required_version = ">= 1.5"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = ">= 5.0"
    }
    google-beta = {
      source  = "hashicorp/google-beta"
      version = ">= 5.0"
    }
    # `time_sleep` で BigLake 接続 SA の IAM 伝播待ちを表現するため（iam.tf）。
    time = {
      source  = "hashicorp/time"
      version = ">= 0.9"
    }
  }
}
