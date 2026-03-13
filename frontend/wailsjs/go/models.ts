export namespace llm {
	
	export class CostEntry {
	    // Go type: time
	    timestamp: any;
	    model: string;
	    input_tokens: number;
	    output_tokens: number;
	    cost_usd: number;
	
	    static createFrom(source: any = {}) {
	        return new CostEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.timestamp = this.convertValues(source["timestamp"], null);
	        this.model = source["model"];
	        this.input_tokens = source["input_tokens"];
	        this.output_tokens = source["output_tokens"];
	        this.cost_usd = source["cost_usd"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class CostSummary {
	    total_input_tokens: number;
	    total_output_tokens: number;
	    total_cost_usd: number;
	    entries: CostEntry[];
	
	    static createFrom(source: any = {}) {
	        return new CostSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.total_input_tokens = source["total_input_tokens"];
	        this.total_output_tokens = source["total_output_tokens"];
	        this.total_cost_usd = source["total_cost_usd"];
	        this.entries = this.convertValues(source["entries"], CostEntry);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace main {
	
	export class ChatResponse {
	    content: string;
	    input_tokens: number;
	    output_tokens: number;
	    cost_usd: number;
	    model: string;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new ChatResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.content = source["content"];
	        this.input_tokens = source["input_tokens"];
	        this.output_tokens = source["output_tokens"];
	        this.cost_usd = source["cost_usd"];
	        this.model = source["model"];
	        this.error = source["error"];
	    }
	}

}

