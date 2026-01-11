const computeDefaultApiBase = () => {
	if (typeof window === 'undefined') {
		// SSR / build-time fallback
		return 'http://localhost:8080';
	}

	const { protocol, hostname, port } = window.location;
	let targetPort = port;

	// When running Vite dev server on port 3000, assume Go API is on 8080 of same host
	if (!import.meta.env.VITE_API_BASE && port === '3000') {
		targetPort = '8080';
	}

	const portSegment = targetPort ? `:${targetPort}` : '';
	return `${protocol}//${hostname}${portSegment}`;
};

export const API_BASE =
	import.meta.env.VITE_API_BASE?.replace(/\/$/, '') ?? computeDefaultApiBase();

