// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import { createApp } from 'vue'
import App from './App.vue'
import './styles.css'

document.documentElement.dataset.platform = navigator.userAgent.includes('Mac') ? 'mac' : 'other'
createApp(App).mount('#app')
