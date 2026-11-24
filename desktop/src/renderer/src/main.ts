// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import { createApp } from 'vue'
import App from './App.vue'
import './styles.css'

document.documentElement.dataset.platform = window.diana.appearance.platform
document.documentElement.dataset.material = window.diana.appearance.material
createApp(App).mount('#app')
