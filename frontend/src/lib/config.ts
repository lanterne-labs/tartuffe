const computeDefaultApiBase = () => {
	if (typeof window === 'undefined') {
		// SSR / build-time fallback
		return 'http://localhost:8080';
	}

	const { protocol, hostname, port } = window.location;
	let targetPort = port;

	// When running Vite dev server (any port that isn't 8080), assume Go API is on 8080
	if (!import.meta.env.VITE_API_BASE && port && port !== '8080') {
		targetPort = '8080';
	}

	const portSegment = targetPort ? `:${targetPort}` : '';
	return `${protocol}//${hostname}${portSegment}`;
};

export const API_BASE =
	import.meta.env.VITE_API_BASE?.replace(/\/$/, '') ?? computeDefaultApiBase();

