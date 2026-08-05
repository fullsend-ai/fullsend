---
description: All Other documentation versions
title: Versions
contributors: false
lastUpdated: false
editLink: false
next: false
---

# Versions

<div
  v-for="link in links"
  :key="link.text"
  class="version-link"
>
  <VPLVersionLink
    :version="link.text"
    :prerelease="link.prerelease"
    :stable="link.stable"
    :edge="link.edge"
  />
</div>

<div>
  <VPLVersionLink :dev="true" :version="aliases.dev" />
</div>

<script setup>
import { useTags, VPLVersionLink } from "@lando/vitepress-theme-default-plus";

const { aliases, links } = useTags();
</script>
