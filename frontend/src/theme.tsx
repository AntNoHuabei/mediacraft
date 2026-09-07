import React, { createContext, useContext, useEffect, useMemo, useState } from 'react'
import { ConfigProvider, theme as antdTheme, type ThemeConfig } from 'antd'

export type ThemeName = 'dark' | 'light'

// 与 styles/console.css 中的 CSS 变量保持一致（antd token 需要具体色值，故这里并列维护一份）。
const PALETTES: Record<ThemeName, { primary: string; bg: string; container: string; elevated: string; border: string; borderSoft: string; text: string; textSec: string; textFaint: string; warn: string; danger: string }> = {
  dark: {
    primary: '#3FE6A4', // VU 绿
    bg: '#0B0C10',
    container: '#12141B',
    elevated: '#181C25',
    border: '#272D39',
    borderSoft: '#1E222B',
    text: '#E9EBEF',
    textSec: '#98A3B2',
    textFaint: '#67707F',
    warn: '#FFC857',
    danger: '#FF6B6B',
  },
  light: {
    primary: '#0E9F6E',
    bg: '#F6F5F1',
    container: '#FFFFFF',
    elevated: '#FFFFFF',
    border: '#E2DFD6',
    borderSoft: '#EEEBE3',
    text: '#23221E',
    textSec: '#6E6A60',
    textFaint: '#A39E92',
    warn: '#B9781D',
    danger: '#D04545',
  },
}

interface ThemeContextValue {
  theme: ThemeName
  setTheme: (name: ThemeName) => void
  toggle: () => void
}

const ThemeContext = createContext<ThemeContextValue>({ theme: 'dark', setTheme: () => {}, toggle: () => {} })

function readInitialTheme(): ThemeName {
  try {
    const saved = localStorage.getItem('mc.theme')
    if (saved === 'light' || saved === 'dark') return saved
  } catch {
    /* ignore */
  }
  return 'dark'
}

export function ThemeProvider({ children }: { children: React.ReactNode }) {
  const [theme, setThemeState] = useState<ThemeName>(readInitialTheme)

  const setTheme = (name: ThemeName) => {
    setThemeState(name)
    try {
      localStorage.setItem('mc.theme', name)
    } catch {
      /* ignore */
    }
  }
  const toggle = () => setTheme(theme === 'dark' ? 'light' : 'dark')

  useEffect(() => {
    document.documentElement.dataset.theme = theme
  }, [theme])

  const palette = PALETTES[theme]
  const config = useMemo<ThemeConfig>(
    () => ({
      algorithm: theme === 'dark' ? antdTheme.darkAlgorithm : antdTheme.defaultAlgorithm,
      token: {
        colorPrimary: palette.primary,
        colorInfo: palette.primary,
        colorSuccess: palette.primary,
        colorWarning: palette.warn,
        colorError: palette.danger,
        colorBgBase: palette.bg,
        colorBgLayout: palette.bg,
        colorBgContainer: palette.container,
        colorBgElevated: palette.elevated,
        colorBorder: palette.border,
        colorBorderSecondary: palette.borderSoft,
        colorTextBase: palette.text,
        colorText: palette.text,
        colorTextSecondary: palette.textSec,
        colorTextTertiary: palette.textFaint,
        colorLink: palette.primary,
        borderRadius: 6,
        fontFamily: "'Segoe UI', 'PingFang SC', 'Microsoft YaHei', system-ui, sans-serif",
      },
    }),
    [theme, palette],
  )

  return (
    <ThemeContext.Provider value={{ theme, setTheme, toggle }}>
      <ConfigProvider theme={config}>{children}</ConfigProvider>
    </ThemeContext.Provider>
  )
}

export function useTheme() {
  return useContext(ThemeContext)
}
