FROM node:20-slim

WORKDIR /app

# Install curl for healthcheck and basic certificates.
RUN apt-get update && apt-get install -y --no-install-recommends \
    curl \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY package*.json ./
RUN npm ci

COPY . .
RUN npm run build

EXPOSE 8001

CMD ["node", "dist/server.js"]
