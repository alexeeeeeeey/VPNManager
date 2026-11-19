const key = 'vpn-manager-theme'
const defaultAccent = '#ffffff'

export function savedAccent() {
  const legacy = localStorage.getItem('vpn-manager-accent')
  const value =
    localStorage.getItem(key) ?? (legacy === '#baf5a7' ? '' : (legacy ?? ''))
  return /^#[0-9a-f]{6}$/i.test(value) ? value : defaultAccent
}

export function applyAccent(value: string) {
  document.documentElement.style.setProperty('--accent', value)
  localStorage.setItem(key, value)
}
