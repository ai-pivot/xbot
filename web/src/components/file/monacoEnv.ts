/**
 * Monaco environment + loader config (Spec 5 §3.3).
 *
 * `@monaco-editor/react` ships Monaco from a CDN by default. We pin it to the
 * locally-installed `monaco-editor` so the app works offline and builds
 * reproducibly. Vite turns `monaco-editor/esm/vs/editor/*.worker?worker` into
 * dedicated web workers; the language workers (ts/json/css/html) keep their
 * rich language services, and `editor.worker` is the generic fallback.
 *
 * Import this module once (it is imported by MonacoEditor via a side-effect
 * re-export) before mounting an editor — `loader.config` must run before the
 * first `<Editor>` instantiates Monaco.
 */
import { loader } from '@monaco-editor/react'
import * as Monaco from 'monaco-editor'

import EditorWorker from 'monaco-editor/esm/vs/editor/editor.worker?worker'
import JsonWorker from 'monaco-editor/esm/vs/language/json/json.worker?worker'
import CssWorker from 'monaco-editor/esm/vs/language/css/css.worker?worker'
import HtmlWorker from 'monaco-editor/esm/vs/language/html/html.worker?worker'
import TsWorker from 'monaco-editor/esm/vs/language/typescript/ts.worker?worker'

// Monaco reads `self.MonacoEnvironment.getWorker` to spawn language services.
self.MonacoEnvironment = {
  getWorker(_workerId: string, label: string) {
    switch (label) {
      case 'json':
        return new JsonWorker()
      case 'css':
      case 'scss':
      case 'less':
        return new CssWorker()
      case 'html':
      case 'handlebars':
      case 'razor':
        return new HtmlWorker()
      case 'typescript':
      case 'javascript':
        return new TsWorker()
      default:
        return new EditorWorker()
    }
  },
}

// Use the locally bundled Monaco instead of the CDN bundle.
loader.config({ monaco: Monaco })

export type MonacoType = typeof Monaco
