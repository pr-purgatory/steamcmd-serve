import adapter from '@sveltejs/adapter-static';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vitest/config';

export default defineConfig({
	plugins: [
		sveltekit({
			adapter: adapter({
				pages: 'dist',
				assets: 'dist',
				fallback: 'index.html',
				precompress: false,
				strict: true
			})
		})
	],
	server: {
		// the dev-only mock imports the games' settings schemas from the repo root
		fs: { allow: ['..'] },
		proxy: {
			'/api': 'http://localhost:8080',
			'/healthz': 'http://localhost:8080'
		}
	},
	test: {
		include: ['src/**/*.test.ts'],
		environment: 'node'
	}
});
