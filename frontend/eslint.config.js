import pluginVue from 'eslint-plugin-vue'
import {
  defineConfigWithVueTs,
  vueTsConfigs,
} from '@vue/eslint-config-typescript'

export default defineConfigWithVueTs(
  { ignores: ['dist/**', 'src/api.gen.ts'] },
  pluginVue.configs['flat/essential'],
  vueTsConfigs.recommended,
  { rules: { 'vue/multi-word-component-names': 'off' } },
)
