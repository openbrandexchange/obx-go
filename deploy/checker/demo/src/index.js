// obx-demo: the public read-only demo portal (obx-conformance mock
// --read-only) with the Kestrel test data, the green reference for the online
// compatibility check. The data lives in the container's memory: a restart
// resets it. Its API key is public on purpose: it can only read demo data.

import { Container } from "@cloudflare/containers";

export class DemoContainer extends Container {
	defaultPort = 8080;
	sleepAfter = "15m";
	enableInternet = false;
	constructor(ctx, env) {
		super(ctx, env);
		this.envVars = { OBX_MOCK_CLIENT: env.DEMO_CLIENT, OBX_PUBLIC_URL: env.PUBLIC_URL };
	}
}

export default {
	async fetch(request, env) {
		const ip = request.headers.get("CF-Connecting-IP") || "unknown";
		const { success } = await env.PER_IP.limit({ key: ip });
		if (!success) {
			return new Response(
				JSON.stringify({ type: "about:blank", title: "Too many requests", status: 429, obx_error: "rate_limited", detail: "slow down" }),
				{ status: 429, headers: { "Content-Type": "application/problem+json", "Retry-After": "60" } },
			);
		}
		const ns = env.DEMO.jurisdiction("eu");
		const demo = ns.get(ns.idFromName("demo"));
		// On a cold start the mock loads the fixtures before it listens: wait
		// up to 45 seconds for it instead of answering 503.
		const body = request.body ? await request.arrayBuffer() : null;
		for (let i = 0; ; i++) {
			const res = await demo.fetch(new Request(request, { body }));
			if (res.status !== 503 || i >= 30) return res;
			await new Promise((r) => setTimeout(r, 1500));
		}
	},
};
