import fs from 'fs'
import path from 'path'
import { createRequire } from 'module'
import { fileURLToPath } from 'url'
import { defineConfig, loadEnv } from '@rsbuild/core'
import { pluginReact } from '@rsbuild/plugin-react'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const require = createRequire(import.meta.url)
const semiUiDir = path.resolve(
  path.dirname(require.resolve('@douyinfe/semi-ui')),
  '../..',
)
// date-fns-tz@1.x needs date-fns v2 (its `_lib/*` internals), but the workspace
// hoists date-fns v4. Locate the v2 copy that ships as date-fns-tz's own
// dependency: the bun store keeps it under
// `.bun/date-fns@2.x/node_modules/date-fns` (symlinked from date-fns-tz), so
// scan the store for the first v2 that has the `_lib/cloneObject` internals the
// alias needs. This is layout-tolerant across bun store hashes.
const resolveDateFnsV2 = () => {
  const workspaceModules = path.resolve(__dirname, '..', 'node_modules')
  const bunStore = path.join(workspaceModules, '.bun')
  const hasCloneObject = (dir: string) =>
    fs.existsSync(path.join(dir, '_lib', 'cloneObject', 'index.js'))
  if (fs.existsSync(bunStore)) {
    for (const entry of fs.readdirSync(bunStore)) {
      if (!entry.startsWith('date-fns@2')) continue
      const candidate = path.join(bunStore, entry, 'node_modules', 'date-fns')
      if (hasCloneObject(candidate)) return candidate
    }
  }
  // Fallback for non-bun installs (nested date-fns under date-fns-tz).
  const tzDir = path.dirname(require.resolve('date-fns-tz/package.json'))
  const nested = path.join(tzDir, 'node_modules', 'date-fns')
  if (hasCloneObject(nested)) return nested
  throw new Error('date-fns v2 (with _lib/cloneObject) not found for date-fns-tz')
}
const semiDateFnsDir = resolveDateFnsV2()

export default defineConfig(({ envMode }) => {
  const env = loadEnv({ mode: envMode, prefixes: ['VITE_'] })
  const clientServerUrl =
    process.env.VITE_REACT_APP_SERVER_URL ||
    env.rawPublicVars.VITE_REACT_APP_SERVER_URL ||
    ''
  const proxyServerUrl =
    clientServerUrl ||
    'http://localhost:3000'
  const isProd = envMode === 'production'
  const devProxy = Object.fromEntries(
    (['/api', '/mj', '/pg'] as const).map((key) => [
      key,
      { target: proxyServerUrl, changeOrigin: true },
    ]),
  ) as Record<string, { target: string; changeOrigin: boolean }>

  return {
    plugins: [pluginReact()],
    source: {
      entry: {
        index: './src/index.jsx',
      },
      define: {
        'import.meta.env.VITE_REACT_APP_SERVER_URL': JSON.stringify(
          clientServerUrl,
        ),
      },
    },
    resolve: {
      alias: {
        '@': path.resolve(__dirname, './src'),
        '@douyinfe/semi-ui/dist/css/semi.css': path.resolve(
          semiUiDir,
          'dist/css/semi.css',
        ),
        'date-fns': semiDateFnsDir,
      },
    },
    html: {
      template: './index.html',
    },
    server: {
      host: '0.0.0.0',
      strictPort: false,
      proxy: devProxy,
    },
    output: {
      minify: isProd,
      target: 'web',
      distPath: {
        root: 'dist',
      },
    },
    performance: {
      removeConsole: isProd ? ['log'] : false,
      buildCache: {
        cacheDigest: [process.env.VITE_REACT_APP_VERSION],
      },
    },
    tools: {
      rspack: {
        module: {
          rules: [
            {
              test: /src[\\/].*\.js$/,
              type: 'javascript/auto',
              use: [
                {
                  loader: 'builtin:swc-loader',
                  options: {
                    jsc: {
                      parser: {
                        syntax: 'ecmascript',
                        jsx: true,
                      },
                      transform: {
                        react: {
                          runtime: 'automatic',
                          development: !isProd,
                          refresh: !isProd,
                        },
                      },
                    },
                  },
                },
              ],
            },
          ],
        },
      },
    },
  }
})
