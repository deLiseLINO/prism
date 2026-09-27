export default {
  async fetch(request, env) {
    if (request.method !== 'POST') return new Response('method not allowed', { status: 405 })
    const report = await request.json()
    if (report.target !== 'issue' && report.target !== 'bot') {
      return new Response('unknown target', { status: 400 })
    }
    if (typeof report.title !== 'string' || typeof report.body !== 'string') {
      return new Response('missing report', { status: 400 })
    }
    if (report.target === 'issue') await postIssue(env, report)
    else await postBot(env, report)
    return new Response(null, { status: 204 })
  },
}

async function postIssue(env, report) {
  const response = await fetch(`https://api.github.com/repos/${env.GITHUB_REPO}/issues`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${env.GITHUB_TOKEN}`,
      accept: 'application/vnd.github+json',
      'user-agent': 'prism-report',
      'content-type': 'application/json',
    },
    body: JSON.stringify({ title: report.title.slice(0, 200), body: report.body }),
  })
  if (!response.ok) throw new Error(`github ${response.status}`)
}

async function postBot(env, report) {
  const form = new FormData()
  form.set('chat_id', env.TELEGRAM_CHAT_ID)
  form.set('caption', report.title.slice(0, 200))
  form.set('document', new File([`${report.body}\n\n${report.log ?? ''}`], 'prism-report.txt', { type: 'text/plain' }))
  const response = await fetch(`https://api.telegram.org/bot${env.TELEGRAM_BOT_TOKEN}/sendDocument`, {
    method: 'POST',
    body: form,
  })
  if (!response.ok) throw new Error(`telegram ${response.status}`)
}
